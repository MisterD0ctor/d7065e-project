#!/usr/bin/env bash
# The full evaluation (report §11): a training week, the model, then the four
# baselines over the same held-out week. Takes ~4.5 h at factor 120.
#
#   deploy/evaluate-all.sh 2>&1 | tee results/evaluate-all.log
set -euo pipefail
cd "$(dirname "$0")/.."

train_start=2026-09-14   # Mon; the training week
eval_start=2026-09-21    # Mon; held out from training
days=5
factor=${FACTOR:-120}

# Occupancy doesn't depend on the controller, so the training week can run
# faster than the evaluation.
deploy/evaluate.sh train-week reactive "$train_start" "$days" 300

echo "$(date +%T) training on train-week"
TRAIN_RUNS=train-week MODEL_NAME=profile-train-week docker compose run --rm --no-deps train

deploy/evaluate.sh eval-reactive   reactive   "$eval_start" "$days" "$factor"
deploy/evaluate.sh eval-constant   constant   "$eval_start" "$days" "$factor"
deploy/evaluate.sh eval-predictive predictive "$eval_start" "$days" "$factor"
deploy/evaluate.sh eval-oracle     oracle     "$eval_start" "$days" "$factor" eval-reactive

# Park the stack on a run of its own, so every eval run's truth can be read.
RUN_ID=idle POLICY=reactive docker compose up -d >/dev/null

mkdir -p results
go run ./cmd/evaluate -runs eval-constant,eval-reactive,eval-predictive,eval-oracle \
  -accuracy-run eval-reactive -out results | tee results/evaluation.md
echo "$(date +%T) evaluation finished"
