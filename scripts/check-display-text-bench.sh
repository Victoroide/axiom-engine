#!/usr/bin/env bash
set -euo pipefail

cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
export LC_ALL=C

go test ./internal/service -run='^$' -bench='^BenchmarkStudyDisplayText$' \
  -benchmem -benchtime=250ms -count=5 -cpu=1 |
  awk '
    function median(key, size,  sorted, i, j, value) {
      for (i = 1; i <= 5; i++) {
        value = timing[key, size, i]
        for (j = i; j > 1 && sorted[j - 1] > value; j--)
          sorted[j] = sorted[j - 1]
        sorted[j] = value
      }
      return sorted[3]
    }
    /^(goos|goarch|cpu):/ { print }
    /^BenchmarkStudyDisplayText\// {
      split($1, name, "/")
      key = name[2] " " name[4]
      size = name[3]
      timing[key, size, ++samples[key, size]] = $3
      if ($5 > 16384 || (key in memory && ($5 != memory[key] || $7 != allocs[key]))) {
        print "Allocation bound failed for " $1 > "/dev/stderr"
        failed = 1
      }
      memory[key] = $5
      allocs[key] = $7
    }
    END {
      print "Fixture and policy       64 KiB ns/op  128 KiB ns/op  Ratio   B/op  allocs/op"
      split("combining emoji filtered", fixture)
      split("legacy display", policy)
      for (i = 1; i <= 3; i++) {
        for (j = 1; j <= 2; j++) {
          key = fixture[i] " " policy[j]
          if (samples[key, "64KiB"] != 5 || samples[key, "128KiB"] != 5) {
            print "Missing benchmark samples for " key > "/dev/stderr"
            failed = 1
            continue
          }
          small = median(key, "64KiB")
          large = median(key, "128KiB")
          ratio = large / small
          printf "%-24s %12.0f  %13.0f  %5.2fx %6d  %9d\n", key, small, large, ratio, memory[key], allocs[key]
          if (ratio > 2.5) {
            print "Scaling bound failed for " key > "/dev/stderr"
            failed = 1
          }
        }
      }
      if (!failed)
        print "PASS: allocation and scaling bounds"
      exit failed
    }
  '
