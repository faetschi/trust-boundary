//go:build !linux

package main

func landlockCheck() check {
	return check{Name: "landlock-abi", Status: warn, Detail: "Landlock ABI can only be measured on Linux"}
}
