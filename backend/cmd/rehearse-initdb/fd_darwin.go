//go:build darwin

package main

import "syscall"

func replaceFD(source, target int) error {
	return syscall.Dup2(source, target)
}
