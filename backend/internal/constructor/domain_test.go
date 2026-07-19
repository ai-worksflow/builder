package constructor

import (
	"encoding/json"
	"testing"
)

func TestApplicationBuildContractJSONExposesAndAuthenticatesContractHash(t *testing.T) {
	t.Parallel()

	compiled, err := (Compiler{}).Compile(readyCompileInput(t))
	if err != nil {
		t.Fatal(err)
	}
	if compiled.ContractHash == "" || compiled.ContractHash != compiled.ContentHash {
		t.Fatalf("compiled hashes = content %q contract %q", compiled.ContentHash, compiled.ContractHash)
	}

	// Leave ContractHash empty to exercise compatibility with persistence
	// adapters that construct the DTO before the explicit field existed.
	value := ApplicationBuildContract{
		ID: "contract-1", ContentHash: compiled.ContentHash, Contract: compiled.Content,
	}
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var representation map[string]any
	if err := json.Unmarshal(payload, &representation); err != nil {
		t.Fatal(err)
	}
	if representation["contractHash"] != compiled.ContractHash {
		t.Fatalf("wire contractHash = %#v, want %q", representation["contractHash"], compiled.ContractHash)
	}

	var decoded ApplicationBuildContract
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ContractHash != compiled.ContractHash {
		t.Fatalf("decoded contractHash = %q, want %q", decoded.ContractHash, compiled.ContractHash)
	}

	representation["contractHash"] = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	tampered, err := json.Marshal(representation)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(tampered, &decoded); err == nil {
		t.Fatal("tampered contractHash was accepted")
	}
	delete(representation, "contractHash")
	missing, err := json.Marshal(representation)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(missing, &decoded); err == nil {
		t.Fatal("missing contractHash was accepted")
	}
}

func TestApplicationBuildContractMarshalRejectsMismatchedContractHash(t *testing.T) {
	t.Parallel()

	compiled, err := (Compiler{}).Compile(readyCompileInput(t))
	if err != nil {
		t.Fatal(err)
	}
	_, err = json.Marshal(ApplicationBuildContract{
		ContractHash: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Contract:     compiled.Content,
	})
	if err == nil {
		t.Fatal("mismatched contractHash was serialized")
	}
}
