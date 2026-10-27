module github.com/akshit-yadav/smpp_simulator/tools/licgen

go 1.26.0

require github.com/akshit-yadav/smpp_simulator/worker v0.0.0

// The licence format lives in the worker module (worker/pkg/licence).
replace github.com/akshit-yadav/smpp_simulator/worker => ../../worker
