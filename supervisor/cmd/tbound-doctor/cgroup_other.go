//go:build !linux

package main

func cgroupCheck() check {
	return check{Name: "cgroup-v2", Status: warn, Detail: "cgroup inventory is Linux-only"}
}
