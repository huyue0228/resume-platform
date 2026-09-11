package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestBundleChecksumsAndExamples(t *testing.T) {
	raw, err := Bundle.ReadFile("bundle/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Version string
		Files   map[string]string
	}
	if err = json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "3.0.0" {
		t.Fatal("unexpected protocol bundle version")
	}
	for name, want := range manifest.Files {
		data, err := Bundle.ReadFile("bundle/" + name)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != want {
			t.Fatalf("generated contract file changed: %s", name)
		}
	}
	for _, name := range []string{"request", "response", "capabilities"} {
		data, err := Bundle.ReadFile("bundle/" + name + ".example.json")
		if err != nil {
			t.Fatal(err)
		}
		if err = Validate(name, data); err != nil {
			t.Fatalf("%s example: %v", name, err)
		}
	}
}
func TestOldPDFProtocolRejected(t *testing.T) {
	raw, _ := Bundle.ReadFile("bundle/request.example.json")
	var request map[string]any
	json.Unmarshal(raw, &request)
	request["protocol_version"] = "resume-analysis/v1"
	changed, _ := json.Marshal(request)
	if Validate("request", changed) == nil {
		t.Fatal("v1 accepted as v2")
	}
	request["protocol_version"] = "resume-analysis/v3"
	scope := request["scope"].(map[string]any)
	delete(scope, "resume_text")
	scope["artifact"] = map[string]any{"path": "file.pdf"}
	changed, _ = json.Marshal(request)
	if Validate("request", changed) == nil {
		t.Fatal("signed PDF artifact accepted by text protocol")
	}
}
