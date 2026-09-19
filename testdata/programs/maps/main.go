package main

type Key struct {
	Name string
	N    int
}

type Point struct{ X, Y int }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	if neg {
		s = "-" + s
	}
	return s
}

// sorted iteration, so output does not depend on map order
func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

func main() {
	// Literals, lookup, comma-ok, len.
	ages := map[string]int{"ann": 30, "bob": 25, "cy": 41}
	println(len(ages), ages["ann"], ages["nobody"])
	v, ok := ages["bob"]
	println(v, ok)
	_, missing := ages["nobody"]
	println(missing)

	// Insert, update, delete.
	ages["dee"] = 19
	ages["ann"] = 31
	delete(ages, "cy")
	delete(ages, "not there")
	println(len(ages), ages["ann"], ages["dee"])

	// Iteration, sorted for a stable comparison.
	total := 0
	for _, k := range sortedKeys(ages) {
		print(k, "=", ages[k], " ")
		total += ages[k]
	}
	println()
	println("total", total)

	// Keys of other types: ints, structs, pointers, arrays, interfaces.
	squares := map[int]int{}
	for i := 1; i <= 5; i++ {
		squares[i*i] = i
	}
	println(len(squares), squares[16], squares[7])

	byKey := map[Key]string{{"a", 1}: "one", {"b", 2}: "two"}
	println(byKey[Key{"a", 1}], byKey[Key{"b", 2}], byKey[Key{"c", 3}] == "")

	p1, p2 := &Point{1, 2}, &Point{1, 2}
	byPtr := map[*Point]string{p1: "first"}
	println(byPtr[p1], byPtr[p2] == "")

	byArr := map[[2]int]bool{{1, 2}: true}
	println(byArr[[2]int{1, 2}], byArr[[2]int{2, 1}])

	byAny := map[any]string{1: "int one", "1": "string one", true: "yes"}
	println(byAny[1], byAny["1"], byAny[true], len(byAny))

	// Values of composite types.
	points := map[string]Point{"origin": {0, 0}, "unit": {1, 1}}
	println(points["unit"].X, points["missing"].X)
	lists := map[string][]int{"a": {1, 2, 3}}
	println(len(lists["a"]), lists["a"][2], lists["missing"] == nil)

	// Maps are references: a copy sees the same entries.
	alias := ages
	alias["eve"] = 7
	println(len(ages), ages["eve"])

	// nil maps read as empty, and assigning to one panics.
	var nilMap map[string]int
	println(nilMap == nil, len(nilMap), nilMap["x"])
	n2, ok2 := nilMap["x"]
	println(n2, ok2)
	for range nilMap {
		println("never")
	}

	// Float keys: NaN is never found again, and -0 matches +0.
	var zero float64
	nan := zero / zero
	fk := map[float64]string{1.5: "a", nan: "b", -0.0: "c"}
	println(fk[1.5], fk[nan] == "", fk[0.0], len(fk))

	// Growth: many entries, then check them all.
	big := make(map[int]string, 4)
	for i := 0; i < 500; i++ {
		big[i] = "v" + itoa(i)
	}
	okAll := true
	for i := 0; i < 500; i++ {
		if big[i] != "v"+itoa(i) {
			okAll = false
		}
	}
	println(len(big), okAll, big[499])
	for i := 0; i < 400; i++ {
		delete(big, i)
	}
	println(len(big), big[400], big[0] == "")

	clear(big)
	println(len(big))

	defer func() { println("recovered:", recover().(error).Error()) }()
	nilMap["boom"] = 1
}
