// 仅用于隔离验收的确定性 TLS 模型替身，不参与平台发布镜像。
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type object = map[string]any

func obj(v any) object { m, _ := v.(map[string]any); return m }
func arr(v any) []any  { a, _ := v.([]any); return a }
func str(v any) string { s, _ := v.(string); return s }
func main() {
	dir := flag.String("dir", "/tmp/resume-go-docker/ca", "temporary trust files")
	address := flag.String("listen", ":56443", "test listener")
	flag.Parse()
	os.MkdirAll(*dir, 0700)
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Resume isolated TLS verification"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true, DNSNames: []string{"host.docker.internal", "localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	certPath, keyPath := filepath.Join(*dir, "fixture-ca.pem"), filepath.Join(*dir, "fixture-key.pem")
	os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0644)
	os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600)
	http.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var request object
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&request) != nil {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		agent := false
		jobs, sections, lines := []any{}, []any{}, []any{}
		for _, message := range arr(request["messages"]) {
			var payload object
			if json.Unmarshal([]byte(str(obj(message)["content"])), &payload) != nil {
				continue
			}
			if payload["available_tools"] != nil {
				agent = true
			}
			for _, v := range arr(payload["result"]) {
				row := obj(v)
				if row["job_ref"] != nil {
					jobs = append(jobs, row)
				} else if row["number"] != nil {
					lines = append(lines, row)
				} else if row["start_line"] != nil {
					sections = append(sections, row)
				}
			}
		}
		turn := object{"ok": true}
		call := func(name string, args object) object { return object{"name": name, "arguments": args} }
		if agent {
			calls := []any{}
			if len(jobs) == 0 || len(sections) == 0 {
				calls = []any{call("resume.list_sections", object{}), call("jobs.list_candidates", object{})}
			} else if len(lines) == 0 {
				calls = []any{call("resume.read_sections", object{"start_line": 1, "end_line": obj(sections[0])["end_line"]})}
			} else {
				var line object
				for _, v := range lines {
					if strings.Contains(str(obj(v)["text"]), "Experience:") {
						line = obj(v)
						break
					}
				}
				if line == nil {
					for _, v := range lines {
						if len(str(obj(v)["text"])) > 20 {
							line = obj(v)
							break
						}
					}
				}
				evidence := object{"quote": str(line["text"]), "page": line["page"], "start_line": line["number"], "end_line": line["number"]}
				matches := []any{}
				for _, j := range jobs {
					matches = append(matches, object{"job_ref": obj(j)["job_ref"], "dimensions": object{"major_match": .8, "skills_match": .8, "experience_evidence": .8, "job_requirement": .8, "resume_quality": .8}, "confidence": .8, "evidence": []any{evidence}, "reason": "合成验收模型：引用原文验证工具循环", "risks": []string{}})
				}
				calls = []any{call("candidate_profile.submit", object{"claims": []any{object{"kind": "project", "summary": "后端服务开发", "evidence": []any{evidence}}}, "risks": []string{}}), call("job_match.submit", object{"matches": matches}), call("task_done", object{"status": "DONE"})}
			}
			turn = object{"kind": "tool_calls", "tool_calls": calls}
		}
		raw, _ := json.Marshal(turn)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(object{"choices": []any{object{"message": object{"content": string(raw)}}}, "usage": object{"prompt_tokens": 100, "completion_tokens": 100}})
	})
	fmt.Println("TLS fixture ready; certificate generated; no real model credentials used")
	server := &http.Server{Addr: *address, ReadHeaderTimeout: 10 * time.Second}
	if err = server.ListenAndServeTLS(certPath, keyPath); err != nil {
		panic(err)
	}
}
