#!/usr/bin/env bash

set -ex

CANDIDATE_SRC="/app/candidate/"
CANDIDATE_BRANCH=$CI_COMMIT_REF_NAME
CANDIDATE_COMMIT_SHA=$CI_COMMIT_SHA

# Clone candidate release
git clone --branch "$CANDIDATE_BRANCH" https://github.com/DataDog/dd-trace-go "$CANDIDATE_SRC" && \
  cd "$CANDIDATE_SRC" && \
  git checkout $CANDIDATE_COMMIT_SHA

# Run benchmarks for candidate release
cd "$CANDIDATE_SRC/ddtrace/tracer/"

for i in {1..10}; do
  taskset --cpu-list 25 \
    go test -run=XXX -bench "BenchmarkConcurrentTracing" -benchmem -count 10 -benchtime 2s ./... | tee "${ARTIFACTS_DIR}/pr_same-cpu-${i}_bench.txt"
done

for i in {1..20}; do
  dedicated_cpu=$((i+24))

  taskset --cpu-list $dedicated_cpu \
    go test -run=XXX -bench "BenchmarkConcurrentTracing" -benchmem -count 10 -benchtime 2s ./... | tee "${ARTIFACTS_DIR}/pr_other-cpu-${dedicated_cpu}_bench.txt"
done
