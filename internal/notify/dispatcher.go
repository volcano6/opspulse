package notify

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Dispatcher coordinates event notifications across all configured channels.
type Dispatcher struct {
	store  *Store
	client *http.Client
}

// NewDispatcher creates a Dispatcher backed by the given channel Store and default HTTP client.
func NewDispatcher(store *Store) *Dispatcher {
	return NewDispatcherWithClient(store, nil)
}

// NewDispatcherWithClient creates a Dispatcher with a custom HTTP client (primarily for testing).
func NewDispatcherWithClient(store *Store, client *http.Client) *Dispatcher {
	return &Dispatcher{
		store:  store,
		client: client,
	}
}

// Dispatch evaluates all configured notification channels and sends the event to matching channels concurrently.
func (d *Dispatcher) Dispatch(ctx context.Context, event Event) []error {
	if d.store == nil {
		return nil
	}

	channels, err := d.store.List()
	if err != nil {
		return []error{fmt.Errorf("failed to read notification channels: %w", err)}
	}

	if len(channels) == 0 {
		return nil
	}

	var matching []Channel
	for _, c := range channels {
		if shouldNotify(c.EffectiveTrigger(), event.Status) {
			matching = append(matching, c)
		}
	}

	if len(matching) == 0 {
		return nil
	}

	var (
		wg      sync.WaitGroup
		errMu   sync.Mutex
		errList []error
	)

	for _, ch := range matching {
		wg.Add(1)
		go func(target Channel) {
			defer wg.Done()
			notifier := NewWebhookNotifier(target, d.client)
			if sendErr := notifier.Send(ctx, event); sendErr != nil {
				errMu.Lock()
				errList = append(errList, sendErr)
				errMu.Unlock()
			}
		}(ch)
	}

	wg.Wait()
	return errList
}

// SendTest dispatches a dummy test event to a specific channel (or all channels if channelName is empty).
func (d *Dispatcher) SendTest(ctx context.Context, channelName string) error {
	if d.store == nil {
		return fmt.Errorf("notifications store is not initialized")
	}

	testEvent := Event{
		JobName:         "test-job",
		Status:          "success",
		Server:          "test-server",
		Snapshot:        "test1234",
		DurationSeconds: 1.23,
		Timestamp:       time.Now(),
	}

	if channelName != "" {
		channel, err := d.store.Get(channelName)
		if err != nil {
			return err
		}
		notifier := NewWebhookNotifier(*channel, d.client)
		return notifier.Send(ctx, testEvent)
	}

	channels, err := d.store.List()
	if err != nil {
		return err
	}
	if len(channels) == 0 {
		return fmt.Errorf("no notification channels configured in %s", d.store.FilePath())
	}

	var firstErr error
	for _, ch := range channels {
		notifier := NewWebhookNotifier(ch, d.client)
		if err := notifier.Send(ctx, testEvent); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func shouldNotify(trigger, status string) bool {
	switch trigger {
	case TriggerAlways:
		return true
	case TriggerSuccess:
		return status == "success"
	case TriggerFailure:
		return status == "failed"
	default:
		return status == "failed"
	}
}
