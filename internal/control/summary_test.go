package control

import (
	"context"
	"testing"

	"github.com/4fuu/box/internal/event"
	"github.com/4fuu/box/internal/secret"
)

func TestSummaryCountsAndOffline(t *testing.T) {
	svc := newSvc(t)
	svc.Metrics = NewMetrics()
	svc.Events = event.New()
	if err := svc.Store.CreateComputer("home", secret.Hash("tok"), "alice"); err != nil {
		t.Fatal(err)
	}
	done := svc.Metrics.Session()
	svc.Metrics.Session()()
	endConsole := svc.Metrics.Console()
	svc.Metrics.Request()
	svc.Metrics.Request()
	svc.Metrics.Denied()
	svc.Metrics.Failed()
	for i := 0; i < recentEvents+2; i++ {
		if _, err := svc.Events.Publish("ssh", "door", "open"); err != nil {
			t.Fatal(err)
		}
	}

	sum, err := svc.Summary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := ServerCounts{Consoles: 1, Sessions: 1, SessionsTotal: 2, Requests: 2, RequestsDenied: 1, RequestsFailed: 1}
	if sum.Server != want {
		t.Fatalf("counts %+v, want %+v", sum.Server, want)
	}
	if sum.Started.IsZero() || sum.Now.Before(sum.Started) {
		t.Fatalf("started %v now %v", sum.Started, sum.Now)
	}
	if len(sum.Machines) != 1 || sum.Machines[0].Name != "home" || sum.Machines[0].Online || sum.Machines[0].Stat != nil {
		t.Fatalf("machines %+v", sum.Machines)
	}
	if sum.EventsTotal != recentEvents+2 || sum.EventsHeld != recentEvents+2 || len(sum.Events) != recentEvents {
		t.Fatalf("events total=%d held=%d recent=%d", sum.EventsTotal, sum.EventsHeld, len(sum.Events))
	}
	if sum.Events[len(sum.Events)-1].ID != recentEvents+2 {
		t.Fatalf("newest event missing: %+v", sum.Events)
	}

	done()
	endConsole()
	sum, _ = svc.Summary(context.Background())
	if sum.Server.Sessions != 0 || sum.Server.Consoles != 0 || sum.Server.SessionsTotal != 2 {
		t.Fatalf("after close %+v", sum.Server)
	}
}

func TestNilMetricsCountNothing(t *testing.T) {
	var m *Metrics
	m.Console()()
	m.Session()()
	m.Request()
	m.Denied()
	m.Failed()
	svc := newSvc(t)
	if _, err := svc.Summary(context.Background()); err != nil {
		t.Fatal(err)
	}
}
