//go:build !linux && !darwin

package export

import "errors"

func cloneFile(src, dest string) error { return errors.New("這個平台不支援 clone") }
