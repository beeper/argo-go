package label

var (
	// 0 / 1 are already FalseMarker / TrueMarker in the label package.
	WireTypeMarkerString  = NullMarker   // −1
	WireTypeMarkerBoolean = AbsentMarker // −2
	WireTypeMarkerVarint  = ErrorMarker  // −3

	WireTypeMarkerFloat64    = NewFromInt64(-4)
	WireTypeMarkerBytes      = NewFromInt64(-5)
	WireTypeMarkerFixed      = NewFromInt64(-6)
	WireTypeMarkerBlock      = NewFromInt64(-7)
	WireTypeMarkerNullable   = NewFromInt64(-8)
	WireTypeMarkerArray      = NewFromInt64(-9)
	WireTypeMarkerRecord     = NewFromInt64(-10)
	WireTypeMarkerDesc       = NewFromInt64(-11)
	WireTypeMarkerError      = NewFromInt64(-12)
	WireTypeMarkerPath       = NewFromInt64(-13)
	WireTypeMarkerUnion      = NewFromInt64(-14)
	WireTypeMarkerExtensions = NewFromInt64(-15)
)
