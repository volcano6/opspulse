package sftp

import "sync"

// runBounded applies fn to items with at most workers calls in flight. Once a
// call fails, no new work is intentionally dispatched; workers already running
// are allowed to finish before the error is returned.
func runBounded[T any](items []T, workers int, fn func(T) error) error {
	if len(items) == 0 {
		return nil
	}
	if workers < 1 {
		workers = 1
	}
	if workers > len(items) {
		workers = len(items)
	}

	jobs := make(chan T)
	stop := make(chan struct{})
	var (
		wg       sync.WaitGroup
		once     sync.Once
		errMu    sync.Mutex
		firstErr error
	)

	worker := func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			case item, ok := <-jobs:
				if !ok {
					return
				}
				// A send can race with a worker reporting an error because both
				// stop and jobs may be ready in the producer's select. Check
				// cancellation again before invoking fn so no post-failure item
				// starts after the in-flight work has been accounted for.
				select {
				case <-stop:
					return
				default:
				}
				if err := fn(item); err != nil {
					once.Do(func() {
						errMu.Lock()
						firstErr = err
						errMu.Unlock()
						close(stop)
					})
					return
				}
			}
		}
	}

	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go worker()
	}

	for _, item := range items {
		select {
		case <-stop:
			close(jobs)
			wg.Wait()
			errMu.Lock()
			err := firstErr
			errMu.Unlock()
			return err
		case jobs <- item:
		}
	}
	close(jobs)
	wg.Wait()

	errMu.Lock()
	defer errMu.Unlock()
	return firstErr
}
