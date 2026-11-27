module github.com/djylb/pathcost/cmd/pathsim

go 1.26

require github.com/djylb/pathcost v0.0.0

// Use the checked-out library when running the simulator with GOWORK=off.
//goland:noinspection VgoMigrateFromReplacesToWorkspace
replace github.com/djylb/pathcost => ../..
