#!/usr/bin/env bash
# The full evaluation (report §11): a training week, the model, then the four
# baselines over the same held-out week. Five runs of about an hour each at
# factor 120.
#
#   SUFFIX=-v2 deploy/evaluate-all.sh 2>&1 | tee results/evaluate-all-v2.log
#
# SUFFIX names this evaluation: storage appends to a run id, so repeating an
# evaluation under the same names would mix its records into the earlier one.
set -euo pipefail
cd "$(dirname "$0")/.."

suffix=${SUFFIX:-}
train_start=2026-09-14   # Mon; the training week
eval_start=2026-09-21    # Mon; held out from training
days=5
factor=${FACTOR:-120}
# The training week runs at the same speed as the evaluation by default, so
# the occupancy sensors report every model minute there too. Occupancy doesn't
# depend on the controller, so a higher TRAIN_FACTOR only thins the readings.
train_factor=${TRAIN_FACTOR:-$factor}

deploy/evaluate.sh "train-week$suffix" reactive "$train_start" "$days" "$train_factor"

echo "$(date +%T) training on train-week$suffix"
train_end=$(date -u -d "$train_start + $days days" +%F)
TRAIN_RUNS="train-week$suffix" MODEL_NAME="profile-train-week$suffix" \
  TRAIN_FROM="${train_start}T00:00:00Z" TRAIN_UNTIL="${train_end}T00:00:00Z" \
  docker compose run --rm --no-deps train 2>&1 | { grep -v '^ Container ' || true; }

deploy/evaluate.sh "eval-reactive$suffix"   reactive   "$eval_start" "$days" "$factor"
deploy/evaluate.sh "eval-constant$suffix"   constant   "$eval_start" "$days" "$factor"
deploy/evaluate.sh "eval-predictive$suffix" predictive "$eval_start" "$days" "$factor"
deploy/evaluate.sh "eval-oracle$suffix"     oracle     "$eval_start" "$days" "$factor" "eval-reactive$suffix"

# Park the stack on a run of its own, so every eval run's truth can be read.
RUN_ID=idle POLICY=reactive docker compose up -d >/dev/null 2>&1

mkdir -p results
go run ./cmd/evaluate \
  -runs "eval-constant$suffix,eval-reactive$suffix,eval-predictive$suffix,eval-oracle$suffix" \
  -accuracy-run "eval-reactive$suffix" -stuck-check -out results | tee "results/evaluation$suffix.md"
echo "$(date +%T) evaluation finished"
