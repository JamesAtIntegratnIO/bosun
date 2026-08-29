#!/usr/bin/env bash
# Deletes the cluster. colima keeps running, because starting it again is the
# slowest part of coming back up and it costs nothing idle.
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

: "${KIND_CLUSTER:=localdev}"
say "deleting kind/${KIND_CLUSTER}"
kind delete cluster --name "$KIND_CLUSTER"
# Single quotes. In double quotes those backticks are command substitution, so
# this line ran `colima stop`, the exact thing the comment above says it does
# not do, and then printed the vm's own shutdown log as though it were part of
# deleting a cluster. The next `make up` paid for it: the slowest step in the
# whole script, silently reintroduced by a sentence about not doing it.
ok 'gone -- run `colima stop` if you want the VM back too'
