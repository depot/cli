#!/bin/sh

run_program() {
  program_name=$1
  shift
  exec "/usr/bin/$program_name" "$@"
}

if [ $# -eq 0 ]; then
    run_program "depot" "$@"
else
  # buildkitd and buildctl are used to support buildx drivers.
  if [ "$1" = "buildkitd" ]; then
    run_program "$@"
  elif [ "$1" = "buildctl" ]; then
    run_program "$@"
  else
    run_program "depot" "$@"
  fi
fi
