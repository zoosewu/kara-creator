//go:build !unix

package app

func diskUsage(string) *Disk { return nil }
