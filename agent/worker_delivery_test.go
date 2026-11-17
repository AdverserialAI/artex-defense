package agent

import (
	"strings"
	"testing"
)

func TestWorkerTemplatesIncludeDeliveryFactContract(t *testing.T) {
	for _, fragment := range []string{"[Deployment]", "record_fact", "host/container", "absolute deployed path", "One separate", "report_finding does not replace deployment registration", "Do not register failed or merely attempted uploads"} {
		if !strings.Contains(workerDefaultTmpl, fragment) {
			t.Errorf("default worker prompt missing %q", fragment)
		}
	}
	if !strings.Contains(workerDefaultTmpl, WorkerDeliveryFactSpec) {
		t.Fatal("default worker prompt must use the shared delivery contract")
	}
	for _, seed := range WorkerDomainSeeds() {
		if !strings.Contains(seed.Body, WorkerDeliveryFactSpec) {
			t.Errorf("scenario %s lost the delivery fact contract", seed.NKey)
		}
	}
}
