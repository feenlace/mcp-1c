package tools

import (
	"context"
	"github.com/feenlace/mcp-1c/epf"
	"github.com/feenlace/mcp-1c/onec"
	"strings"
	"testing"
	"time"
)

func TestEPFWaitErrorUsesActualBudget(t *testing.T) {
	for _, earlier := range []bool{false, true} {
		err := &onec.TransportError{Timeout: 5 * time.Minute, Err: &epf.WaitTimeoutError{Budget: 600 * time.Second, EarlierDeadline: earlier, Cause: context.DeadlineExceeded}}
		text := renderFailure("Ошибка", err)
		if !strings.Contains(text, "10m0s") || strings.Contains(text, "5m0s") {
			t.Fatal("wrong timeout", text)
		}
		if earlier && !strings.Contains(text, "вызывающей стороны") {
			t.Fatal("lost caller limit", text)
		}
		if !earlier && !strings.Contains(text, "Ожидание ответа, сек.") {
			t.Fatal("lost form remedy", text)
		}
	}
}
