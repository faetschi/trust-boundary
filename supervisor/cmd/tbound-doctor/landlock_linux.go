package main

import "tbound/supervisor/internal/piruntime"

func landlockCheck() check {
	abi, err := piruntime.D09LandlockKernelABI()
	return landlockCheckForABI(abi, err, piruntime.D09LandlockABI)
}
