package platform

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	c "resume-platform/internal/contract"
	"slices"
	"sort"
	"time"
)

const allocationProtocol = "resume-allocation/v1"
const allocationResult = "resume-allocation-plan/v1"
const allocationPolicy = "allocation-order/v1"

func allocationRef(kind string, id any) string { return kind + "-" + str(id) }
func allocationSnapshotHash(snapshot c.AllocationSnapshot) string {
	raw, _ := json.Marshal(snapshot)
	var s c.AllocationSnapshot
	_ = json.Unmarshal(raw, &s)
	sort.Slice(s.Members, func(i, j int) bool { return s.Members[i].MemberID < s.Members[j].MemberID })
	sort.Slice(s.Demands, func(i, j int) bool { return s.Demands[i].DemandID < s.Demands[j].DemandID })
	for i := range s.Members {
		m := &s.Members[i]
		sort.Slice(m.Tags, func(i, j int) bool { return m.Tags[i].Code < m.Tags[j].Code })
		sort.Slice(m.CountedDemandIDs, func(i, j int) bool { return m.CountedDemandIDs[i] < m.CountedDemandIDs[j] })
		sort.Slice(m.AllowedDemandIDs, func(i, j int) bool { return m.AllowedDemandIDs[i] < m.AllowedDemandIDs[j] })
	}
	for i := range s.Demands {
		sort.Strings(s.Demands[i].RequiredTags)
		sort.Strings(s.Demands[i].PreferredTags)
	}
	raw, _ = json.Marshal(s)
	var value any
	_ = json.Unmarshal(raw, &value)
	raw, _ = json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Recompute independently at the business boundary. Never accept the Kernel's claimed score/order.
func expectedAllocation(snapshot c.AllocationSnapshot) []c.AllocationDecision {
	members := append([]c.AllocationMember{}, snapshot.Members...)
	sort.Slice(members, func(i, j int) bool {
		left, _ := time.Parse(time.RFC3339Nano, members[i].CreatedAt)
		right, _ := time.Parse(time.RFC3339Nano, members[j].CreatedAt)
		if left.Equal(right) {
			return members[i].MemberID < members[j].MemberID
		}
		return left.Before(right)
	})
	supply := map[int64]int64{}
	last := map[int64]int64{}
	for _, d := range snapshot.Demands {
		supply[d.DemandID] = d.RecentSupplyCount
		last[d.DemandID] = d.LastAllocationSequence
	}
	sequence := snapshot.NextSequence
	decisions := []c.AllocationDecision{}
	for _, m := range members {
		result := c.AllocationDecision{MemberID: m.MemberID, QualificationRef: m.QualificationRef, Action: "wait", MatchedTags: []string{}, AssertionRefs: []string{}, Order: []int64{}, ReasonCode: "no_active_mapping"}
		if len(m.AllowedDemandIDs) > 0 {
			result.ReasonCode = "no_receiving_demand"
		}
		tags := map[string]string{}
		for _, t := range m.Tags {
			if t.Verified && t.Status == "supported" && (t.Source == "manual" || t.ConfidenceBPS >= 8000) {
				tags[t.Code] = t.AssertionRef
			}
		}
		for _, d := range snapshot.Demands {
			allowed := false
			for _, id := range m.AllowedDemandIDs {
				if id == d.DemandID {
					allowed = true
					break
				}
			}
			if !allowed || d.ReceptionState != "receiving" {
				continue
			}
			if result.Action == "wait" {
				result.ReasonCode = "required_tags_unavailable"
			}
			matched := map[string]bool{}
			all := true
			var preferred int64
			for _, code := range d.RequiredTags {
				if tags[code] == "" {
					all = false
					break
				}
				matched[code] = true
			}
			if !all {
				continue
			}
			for _, code := range d.PreferredTags {
				if tags[code] != "" {
					matched[code] = true
					preferred++
				}
			}
			if len(matched) == 0 {
				continue
			}
			order := []int64{-preferred, d.Priority, supply[d.DemandID], last[d.DemandID], d.DemandID}
			better := result.Action == "wait"
			if !better {
				for i, n := range order {
					if n != result.Order[i] {
						better = n < result.Order[i]
						break
					}
				}
			}
			if !better {
				continue
			}
			keys := []string{}
			for k := range matched {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			refs := []string{}
			for _, k := range keys {
				refs = append(refs, tags[k])
			}
			result = c.AllocationDecision{MemberID: m.MemberID, QualificationRef: m.QualificationRef, Action: "assign", DemandID: d.DemandID, DepartmentRef: d.DepartmentRef, MatchedTags: keys, AssertionRefs: refs, Order: order, Sequence: sequence, ReasonCode: "allocated"}
		}
		if result.Action == "assign" {
			if !slices.Contains(m.CountedDemandIDs, result.DemandID) {
				supply[result.DemandID]++
			}
			last[result.DemandID] = sequence
			sequence++
		}
		decisions = append(decisions, result)
	}
	return decisions
}
func validateAllocationResult(request c.AllocationRequest, result c.AllocationResponse) error {
	raw, err := json.Marshal(result)
	if err != nil || c.Validate("allocation.response", raw) != nil {
		return taskError("allocation_output_invalid", "分配结果协议无效")
	}
	if result.TaskID != request.TaskID || result.IdempotencyKey != request.IdempotencyKey || result.SnapshotHash != request.SnapshotHash || !reflect.DeepEqual(result.Pin, request.Pin) || result.TerminalState != "DONE" {
		return taskError("allocation_output_invalid", "分配结果与冻结任务不一致")
	}
	expected := expectedAllocation(request.Snapshot)
	if !reflect.DeepEqual(expected, result.Decisions) {
		return taskError("allocation_output_invalid", "分配方案未通过平台独立优先级复算")
	}
	return nil
}
func allocationReason(code string) string {
	switch code {
	case "allocated":
		return "已归属内部用人需求"
	case "no_active_mapping":
		return "等待配置内部需求映射"
	case "no_receiving_demand":
		return "等待需求接收"
	case "required_tags_unavailable":
		return "缺少可用标签，保留筛选通过资格"
	case "qualification_stale":
		return "资格版本已变化，需要重新筛选"
	case "allocation_snapshot_stale":
		return "分配期间数据变化，请重试"
	case "allocation_timeout":
		return "分配计算超时，可独立重试"
	case "kernel_unavailable":
		return "分配服务暂不可用"
	}
	return fmt.Sprintf("分配待处理（%s）", code)
}

func allocationTagHash(tags []c.AllocationTag) string {
	values := append([]c.AllocationTag{}, tags...)
	sort.Slice(values, func(i, j int) bool { return values[i].Code < values[j].Code })
	raw, _ := json.Marshal(values)
	var value any
	_ = json.Unmarshal(raw, &value)
	raw, _ = json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
