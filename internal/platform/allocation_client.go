package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	c "resume-platform/internal/contract"
	"strings"
	"time"
)

func (a *App) allocationHTTP(ctx context.Context, method, path string, body any) ([]byte, error) {
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil || len(raw) > 2<<20 {
			return nil, taskError("allocation_request_too_large", "分配快照超过请求上限")
		}
	}
	if a.Config.KernelToken == "" {
		return nil, taskError("kernel_unavailable", "分配 Kernel 尚未配置")
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(a.Config.KernelURL, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Agent-Kernel-Token", a.Config.KernelToken)
	req.Header.Set("Content-Type", "application/json")
	client := *a.HTTP
	client.Timeout = 35 * time.Second
	res, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, taskError("allocation_timeout", "分配请求超时")
		}
		return nil, taskError("kernel_unavailable", "分配服务连接失败")
	}
	defer res.Body.Close()
	raw, err = io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 {
		return nil, taskError("allocation_output_invalid", "分配服务返回无效结果")
	}
	if res.StatusCode != 200 {
		var body Object
		_ = json.Unmarshal(raw, &body)
		code := str(body["code"])
		if !contains([]string{"allocation_snapshot_stale", "kernel_version_unavailable", "idempotency_conflict", "allocation_timeout", "agent_budget_exhausted"}, code) {
			code = "kernel_unavailable"
		}
		return nil, taskError(code, allocationReason(code))
	}
	return raw, nil
}
func (a *App) allocationPin(ctx context.Context) (c.AllocationPin, error) {
	var pin c.AllocationPin
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	raw, err := a.allocationHTTP(ctx, "GET", "/v2/allocation/capabilities", nil)
	if err != nil {
		return pin, err
	}
	if c.Validate("allocation.capabilities", raw) != nil {
		return pin, taskError("agent_protocol_incompatible", "分配协议不兼容")
	}
	var caps c.AllocationCapabilities
	_ = json.Unmarshal(raw, &caps)
	if caps.Mock != (a.Config.Debug && boolEnv("AGENT_KERNEL_ALLOW_MOCK")) || (a.Config.KernelBuild != "" && caps.KernelBuild != a.Config.KernelBuild) || caps.ToolsetVersion != "allocation-tools/v1" || caps.PolicyVersion != allocationPolicy || caps.InstructionVersion != "allocation-deterministic/v1" {
		return pin, taskError("kernel_version_unavailable", "分配能力版本不匹配")
	}
	pin = c.AllocationPin{ProtocolVersion: allocationProtocol, ResultSchemaVersion: allocationResult, KernelBuild: caps.KernelBuild, ToolsetVersion: caps.ToolsetVersion, PolicyVersion: caps.PolicyVersion, InstructionVersion: caps.InstructionVersion}
	pin.PinID = fingerprint(pin)
	return pin, nil
}
func (a *App) callAllocation(ctx context.Context, request c.AllocationRequest) (c.AllocationResponse, error) {
	var result c.AllocationResponse
	raw, err := json.Marshal(request)
	if err != nil || c.Validate("allocation.request", raw) != nil {
		return result, taskError("allocation_snapshot_invalid", "分配快照不符合协议")
	}
	raw, err = a.allocationHTTP(ctx, "POST", "/v2/allocation/tasks/execute", request)
	if err != nil {
		return result, err
	}
	if c.Validate("allocation.response", raw) != nil || json.Unmarshal(raw, &result) != nil {
		return result, taskError("allocation_output_invalid", "分配返回格式无效")
	}
	return result, validateAllocationResult(request, result)
}
