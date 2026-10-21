module github.com/akshit-yadav/smpp_simulator/loadgen

go 1.26.0

require (
	github.com/HdrHistogram/hdrhistogram-go v1.4.0
	github.com/akshit-yadav/smpp_simulator/worker v0.0.0
)

require github.com/stretchr/testify v1.12.1 // indirect

// The SMPP codec lives in the worker module.
replace github.com/akshit-yadav/smpp_simulator/worker => ../worker
