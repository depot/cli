package retry

import "time"

func Retry(fn func() error, attempts int) error {
	var err error
	for i := 0; i < attempts; i++ {
		err = fn()
		if err == nil {
			return nil
		}
		time.Sleep(1 * time.Second)
	}
	return err
}
