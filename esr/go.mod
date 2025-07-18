module github.com/sdoque/systems/esr

go 1.24.4

require github.com/sdoque/mbaigo v0.0.0-20250520155324-7390c339652a

// Replaces this library with a patched version
replace github.com/sdoque/mbaigo v0.0.0-20250520155324-7390c339652a => github.com/lmas/mbaigo v0.0.0-20250715100940-0fef178d190b
