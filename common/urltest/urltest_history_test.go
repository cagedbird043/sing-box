package urltest

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/common/observable"
)

func TestHistoryStorageTargetIsolation(t *testing.T) {
	storage := NewHistoryStorage()
	first := &adapter.URLTestHistory{Time: time.Unix(100, 0), Delay: 10}
	second := &adapter.URLTestHistory{Time: time.Unix(200, 0), Delay: 20}
	other := &adapter.URLTestHistory{Time: time.Unix(300, 0), Delay: 30}
	storage.StoreURLTestHistory("node", "https://first.example/", storage.OutboundGeneration("node"), first)
	storage.StoreURLTestHistory("node", "https://second.example/", storage.OutboundGeneration("node"), second)
	storage.StoreURLTestHistory("other", "https://first.example/", storage.OutboundGeneration("other"), other)

	assertHistory(t, storage.LoadURLTestHistory("node", "https://first.example/"), first)
	assertHistory(t, storage.LoadURLTestHistory("node", "https://second.example/"), second)
	assertHistory(t, storage.LoadURLTestHistory("node", "https://missing.example/"), nil)
	assertHistory(t, storage.LoadLatestURLTestHistory("node"), second)

	replacement := &adapter.URLTestHistory{Time: time.Unix(150, 0), Delay: 15}
	storage.StoreURLTestHistory("node", "https://first.example/", storage.OutboundGeneration("node"), replacement)
	assertHistory(t, storage.LoadURLTestHistory("node", "https://first.example/"), replacement)
	assertHistory(t, storage.LoadURLTestHistory("node", "https://second.example/"), second)
	assertHistory(t, storage.LoadLatestURLTestHistory("node"), second)
	if first.Time != time.Unix(100, 0) || first.Delay != 10 {
		t.Fatal("overwriting a target mutated a previously returned history")
	}

	// A failed probe deletes only the failed target, not another target or node.
	storage.DeleteURLTestHistory("node", "https://first.example/", storage.OutboundGeneration("node"))
	assertHistory(t, storage.LoadURLTestHistory("node", "https://first.example/"), nil)
	assertHistory(t, storage.LoadURLTestHistory("node", "https://second.example/"), second)
	assertHistory(t, storage.LoadURLTestHistory("other", "https://first.example/"), other)
	assertHistory(t, storage.LoadLatestURLTestHistory("node"), second)
	if replacement.Time != time.Unix(150, 0) || replacement.Delay != 15 {
		t.Fatal("deleting a target mutated a previously returned history")
	}
}

func TestHistoryStorageLatestRemaining(t *testing.T) {
	storage := NewHistoryStorage()
	oldest := &adapter.URLTestHistory{Time: time.Unix(100, 0), Delay: 10}
	middle := &adapter.URLTestHistory{Time: time.Unix(200, 0), Delay: 20}
	newest := &adapter.URLTestHistory{Time: time.Unix(300, 0), Delay: 30}
	// Completion order need not match the successful probe timestamps.
	storage.StoreURLTestHistory("node", "newest", storage.OutboundGeneration("node"), newest)
	storage.StoreURLTestHistory("node", "oldest", storage.OutboundGeneration("node"), oldest)
	storage.StoreURLTestHistory("node", "middle", storage.OutboundGeneration("node"), middle)
	assertHistory(t, storage.LoadLatestURLTestHistory("node"), newest)

	storage.DeleteURLTestHistory("node", "missing", storage.OutboundGeneration("node"))
	storage.DeleteURLTestHistory("node", "oldest", storage.OutboundGeneration("node"))
	assertHistory(t, storage.LoadLatestURLTestHistory("node"), newest)
	storage.DeleteURLTestHistory("node", "newest", storage.OutboundGeneration("node"))
	assertHistory(t, storage.LoadLatestURLTestHistory("node"), middle)
	assertHistory(t, storage.LoadURLTestHistory("node", "newest"), nil)
	storage.DeleteURLTestHistory("node", "middle", storage.OutboundGeneration("node"))
	assertHistory(t, storage.LoadLatestURLTestHistory("node"), nil)
}

func TestHistoryStorageLatestOverwrite(t *testing.T) {
	storage := NewHistoryStorage()
	first := &adapter.URLTestHistory{Time: time.Unix(200, 0), Delay: 20}
	second := &adapter.URLTestHistory{Time: time.Unix(300, 0), Delay: 30}
	storage.StoreURLTestHistory("node", "first", storage.OutboundGeneration("node"), first)
	storage.StoreURLTestHistory("node", "second", storage.OutboundGeneration("node"), second)

	older := &adapter.URLTestHistory{Time: time.Unix(100, 0), Delay: 10}
	storage.StoreURLTestHistory("node", "second", storage.OutboundGeneration("node"), older)
	assertHistory(t, storage.LoadLatestURLTestHistory("node"), first)
	assertHistory(t, storage.LoadURLTestHistory("node", "second"), older)

	storage.StoreURLTestHistory("node", "first", storage.OutboundGeneration("node"), nil)
	assertHistory(t, storage.LoadURLTestHistory("node", "first"), nil)
	assertHistory(t, storage.LoadLatestURLTestHistory("node"), older)
	storage.StoreURLTestHistory("node", "second", storage.OutboundGeneration("node"), nil)
	assertHistory(t, storage.LoadLatestURLTestHistory("node"), nil)
}

func TestHistoryStorageDefaultURLAlias(t *testing.T) {
	storage := NewHistoryStorage()
	first := &adapter.URLTestHistory{Time: time.Unix(100, 0), Delay: 10}
	second := &adapter.URLTestHistory{Time: time.Unix(200, 0), Delay: 20}
	storage.StoreURLTestHistory("node", "", storage.OutboundGeneration("node"), first)
	assertHistory(t, storage.LoadURLTestHistory("node", DefaultURL), first)
	storage.StoreURLTestHistory("node", DefaultURL, storage.OutboundGeneration("node"), second)
	assertHistory(t, storage.LoadURLTestHistory("node", ""), second)

	// Only the empty URL is normalized; distinct nonempty targets stay distinct.
	explicit := DefaultURL + "/"
	storage.StoreURLTestHistory("node", explicit, storage.OutboundGeneration("node"), first)
	assertHistory(t, storage.LoadURLTestHistory("node", explicit), first)
	assertHistory(t, storage.LoadURLTestHistory("node", DefaultURL), second)
	storage.DeleteURLTestHistory("node", "", storage.OutboundGeneration("node"))
	assertHistory(t, storage.LoadURLTestHistory("node", DefaultURL), nil)
	assertHistory(t, storage.LoadLatestURLTestHistory("node"), first)
	storage.StoreURLTestHistory("node", "", storage.OutboundGeneration("node"), second)
	storage.DeleteURLTestHistory("node", DefaultURL, storage.OutboundGeneration("node"))
	assertHistory(t, storage.LoadURLTestHistory("node", ""), nil)
	assertHistory(t, storage.LoadURLTestHistory("node", explicit), first)
}

func TestHistoryStorageDeleteOutbound(t *testing.T) {
	storage := NewHistoryStorage()
	history := &adapter.URLTestHistory{Time: time.Unix(100, 0), Delay: 10}
	targets := []string{"", "https://first.example/", "https://second.example/"}
	for _, target := range targets {
		storage.StoreURLTestHistory("node", target, storage.OutboundGeneration("node"), history)
		storage.StoreURLTestHistory("other", target, storage.OutboundGeneration("other"), history)
	}
	storage.DeleteOutboundURLTestHistory("missing")
	storage.DeleteOutboundURLTestHistory("node")
	for _, target := range targets {
		assertHistory(t, storage.LoadURLTestHistory("node", target), nil)
		assertHistory(t, storage.LoadURLTestHistory("other", target), history)
	}
	assertHistory(t, storage.LoadLatestURLTestHistory("node"), nil)
	assertHistory(t, storage.LoadLatestURLTestHistory("other"), history)

	replacement := &adapter.URLTestHistory{Time: time.Unix(200, 0), Delay: 20}
	storage.StoreURLTestHistory("node", "https://new.example/", storage.OutboundGeneration("node"), replacement)
	assertHistory(t, storage.LoadLatestURLTestHistory("node"), replacement)
	for _, target := range targets {
		assertHistory(t, storage.LoadURLTestHistory("node", target), nil)
	}
}

func TestHistoryStorageRejectsStaleGeneration(t *testing.T) {
	for _, seeded := range []bool{false, true} {
		name := "without-history"
		if seeded {
			name = "with-history"
		}
		t.Run(name, func(t *testing.T) {
			storage := NewHistoryStorage()
			oldGeneration := storage.OutboundGeneration("node")
			old := &adapter.URLTestHistory{Time: time.Unix(100, 0), Delay: 10}
			if seeded {
				storage.StoreURLTestHistory("node", "", oldGeneration, old)
			}
			storage.DeleteOutboundURLTestHistory("node")
			generation := storage.OutboundGeneration("node")
			if generation != oldGeneration+1 {
				t.Fatalf("generation = %d, want %d", generation, oldGeneration+1)
			}
			if storage.OutboundGeneration("other") != 0 {
				t.Fatal("deleting a node changed another node's generation")
			}
			storage.StoreURLTestHistory("node", "", oldGeneration, old)
			assertHistory(t, storage.LoadURLTestHistory("node", DefaultURL), nil)
			assertHistory(t, storage.LoadLatestURLTestHistory("node"), nil)

			fresh := &adapter.URLTestHistory{Time: time.Unix(200, 0), Delay: 20}
			storage.StoreURLTestHistory("node", DefaultURL, generation, fresh)
			hook := observable.NewSubscriber[struct{}](8)
			defer hook.Close()
			storage.AddUpdateHook(hook)
			updates, _ := hook.Subscription()
			resume := make(chan struct{})
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				<-resume
				storage.StoreURLTestHistory("node", "", oldGeneration, old)
				storage.StoreURLTestHistory("node", "stale-target", oldGeneration, old)
				storage.DeleteURLTestHistory("node", DefaultURL, oldGeneration)
			}()
			close(resume)
			<-finished
			assertHistory(t, storage.LoadURLTestHistory("node", ""), fresh)
			assertHistory(t, storage.LoadURLTestHistory("node", "stale-target"), nil)
			assertHistory(t, storage.LoadLatestURLTestHistory("node"), fresh)
			select {
			case <-updates:
				t.Fatal("a rejected stale result emitted an update")
			default:
			}
			storage.DeleteURLTestHistory("node", "", generation)
			assertHistory(t, storage.LoadLatestURLTestHistory("node"), nil)
			select {
			case <-updates:
			default:
				t.Fatal("a current-generation deletion did not emit an update")
			}
		})
	}
}

func TestHistoryStorageNilLoads(t *testing.T) {
	var storage *HistoryStorage
	assertHistory(t, storage.LoadURLTestHistory("node", ""), nil)
	assertHistory(t, storage.LoadLatestURLTestHistory("node"), nil)
}

func TestHistoryStorageConcurrentTargets(t *testing.T) {
	const (
		workers = 8
		rounds  = 64
	)
	storage := NewHistoryStorage()
	var group sync.WaitGroup
	start := make(chan struct{})
	for worker := range workers {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			<-start
			target := "https://target.example/" + strconv.Itoa(worker)
			for round := range rounds {
				history := &adapter.URLTestHistory{
					Time:  time.Unix(int64(round*workers+worker), 0),
					Delay: uint16(worker + 1),
				}
				storage.StoreURLTestHistory("node", target, storage.OutboundGeneration("node"), history)
				if loaded := storage.LoadURLTestHistory("node", target); loaded != history {
					t.Errorf("worker %d lost its target to another writer", worker)
					return
				}
				if latest := storage.LoadLatestURLTestHistory("node"); latest == nil {
					t.Errorf("worker %d could not display a live successful target", worker)
					return
				}
				storage.DeleteURLTestHistory("node", target, storage.OutboundGeneration("node"))
				if loaded := storage.LoadURLTestHistory("node", target); loaded != nil {
					t.Errorf("worker %d saw a deleted target", worker)
					return
				}
			}
			storage.StoreURLTestHistory("node", target, storage.OutboundGeneration("node"), &adapter.URLTestHistory{
				Time:  time.Unix(int64(rounds*workers+worker), 0),
				Delay: uint16(worker + 1),
			})
		}(worker)
	}
	close(start)
	group.Wait()
	for worker := range workers {
		target := "https://target.example/" + strconv.Itoa(worker)
		history := storage.LoadURLTestHistory("node", target)
		if history == nil || history.Time != time.Unix(int64(rounds*workers+worker), 0) || history.Delay != uint16(worker+1) {
			t.Fatalf("worker %d final history = %+v", worker, history)
		}
	}
	latestTarget := "https://target.example/" + strconv.Itoa(workers-1)
	assertHistory(t, storage.LoadLatestURLTestHistory("node"), storage.LoadURLTestHistory("node", latestTarget))
	storage.DeleteURLTestHistory("node", latestTarget, storage.OutboundGeneration("node"))
	nextTarget := "https://target.example/" + strconv.Itoa(workers-2)
	assertHistory(t, storage.LoadLatestURLTestHistory("node"), storage.LoadURLTestHistory("node", nextTarget))
	storage.DeleteOutboundURLTestHistory("node")
	assertHistory(t, storage.LoadLatestURLTestHistory("node"), nil)
}

func assertHistory(t *testing.T, actual, expected *adapter.URLTestHistory) {
	t.Helper()
	if actual != expected {
		t.Fatalf("history = %p (%+v), want %p (%+v)", actual, actual, expected, expected)
	}
}
