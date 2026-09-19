package dep

// Base is read by another package only in that package's initializers.
var Base = compute()

func compute() int { return 40 }

// Unused is read by nothing at all; building it must still run its call.
var Unused = sideEffect()

func sideEffect() int {
	println("dep: side effect of an unused variable's initializer")
	return 1
}
