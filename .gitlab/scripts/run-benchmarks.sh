#!/usr/bin/env bash

set -ex

source ./.gitlab/scripts/config-benchmarks.sh

CANDIDATE_BRANCH=$CI_COMMIT_REF_NAME
CANDIDATE_COMMIT_SHA=$CI_COMMIT_SHA

# Clone candidate release
git clone --branch "$CANDIDATE_BRANCH" https://github.com/DataDog/dd-trace-go "$CANDIDATE_SRC" && \
  cd "$CANDIDATE_SRC" && \
  git checkout $CANDIDATE_COMMIT_SHA

# Run benchmarks for candidate release
cd "$CANDIDATE_SRC/ddtrace/tracer/"
#go test -run=XXX -bench $BENCHMARK_TARGETS -benchmem -count 10 -benchtime 2s ./... | tee "${ARTIFACTS_DIR}/pr_bench.txt"
go test -c -run=XXX -bench "BenchmarkConcurrentTracing" -benchmem -count 10 -benchtime 2s ./...
cp tracer.test "${ARTIFACTS_DIR}/pr.tracer.test"

if [ ! -z "$BASELINE_BRANCH" ]; then
  cd "$CANDIDATE_SRC"

  # Clone baseline release
  git clone --branch "$BASELINE_BRANCH" https://github.com/DataDog/dd-trace-go/ "$BASELINE_SRC" && \
    cd "$BASELINE_SRC" && \
    git checkout $BASELINE_COMMIT_SHA

  # Run benchmarks for baseline release
  cd "$BASELINE_SRC/ddtrace/tracer/"
  go test -c -run=XXX -bench "BenchmarkConcurrentTracing" -benchmem -count 10 -benchtime 2s ./...
  cp tracer.test "${ARTIFACTS_DIR}/main.tracer.test"
  #go test -run=XXX -bench $BENCHMARK_TARGETS -benchmem -count 10 -benchtime 2s ./... | tee "${ARTIFACTS_DIR}/main_bench.txt"
fi
