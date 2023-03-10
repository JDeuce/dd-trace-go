#!/usr/bin/env bash

# Change threshold for detection of regression
# @see https://github.com/DataDog/relenv-benchmark-analyzer#what-is-a-significant-difference
export UNCONFIDENCE_THRESHOLD=2.0

CANDIDATE_BRANCH=$CI_COMMIT_REF_NAME
CANDIDATE_SRC="/app/candidate/"

cd "$CANDIDATE_SRC"
CANDIDATE_COMMIT_SHA=$(git rev-parse --short HEAD)

benchmark_analyzer convert \
  --framework=GoBench \
  --extra-params="{\
    \"config\":\"candidate\", \
    \"git_commit_sha\":\"$CANDIDATE_COMMIT_SHA\", \
    \"git_branch\":\"$CANDIDATE_BRANCH\"\
    }" \
  --outpath="${ARTIFACTS_DIR}/pr.converted.json" \
  "${ARTIFACTS_DIR}/pr_bench.txt"
