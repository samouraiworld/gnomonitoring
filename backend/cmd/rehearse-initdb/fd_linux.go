//go:build linux

package main

import "syscall"

func replaceFD(source, target int) error {
	return syscall.Dup3(source, target, 0)
}
