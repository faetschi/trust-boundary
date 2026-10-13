#!/usr/bin/env bash
# Shared report classifier for bootstrap-linux.sh and offline regression cases.

tbound_normalize_decimal() {
  local value=${1-}
  [[ "$value" =~ ^[0-9]+$ ]] || return 1
  while [[ ${#value} -gt 1 && ${value:0:1} == 0 ]]; do
    value=${value:1}
  done
  printf '%s\n' "$value"
}

tbound_report_landlock_abi() {
  local report_file=$1 line status name detail raw_abi
  local found=0
  while IFS= read -r line || [[ -n "$line" ]]; do
    if [[ "$line" =~ ^(PASS|FAIL|UNVERIFIED)[[:space:]]+([A-Za-z0-9_.-]+):[[:space:]]*(.*)$ ]]; then
      status=${BASH_REMATCH[1]}
      name=${BASH_REMATCH[2]}
      detail=${BASH_REMATCH[3]}
      if [[ "$name" == landlock-abi ]]; then
        (( found += 1 ))
        [[ "$status" == PASS ]] || return 1
        [[ "$detail" =~ ABI[[:space:]]+([0-9]+) ]] || return 1
        raw_abi=${BASH_REMATCH[1]}
      fi
    fi
  done < "$report_file"
  [[ "$found" -eq 1 ]] || return 1
  tbound_normalize_decimal "$raw_abi"
}

# Prints the bootstrap result code. The report is authoritative for row status;
# probe_rc is also checked for contradictory or malformed probe output.
tbound_classify_probe_report() {
  local report_file=$1 probe_rc=${2-}
  local line status name detail raw_abi= normalized_abi=
  local row_count=0 fail_count=0 unverified_count=0 unexpected_unverified=0
  local generic_count=0 generic_status= d09_count=0 d09_fail_count=0
  local d09_detail= d09_status=

  if [[ ! -f "$report_file" || ! -r "$report_file" ]]; then
    printf '2\n'
    return 0
  fi

  while IFS= read -r line || [[ -n "$line" ]]; do
    if [[ "$line" =~ ^(PASS|FAIL|UNVERIFIED)[[:space:]]+([A-Za-z0-9_.-]+):[[:space:]]*(.*)$ ]]; then
      (( row_count += 1 ))
      status=${BASH_REMATCH[1]}
      name=${BASH_REMATCH[2]}
      detail=${BASH_REMATCH[3]}
      case "$status" in
        FAIL) (( fail_count += 1 )) ;;
        UNVERIFIED)
          (( unverified_count += 1 ))
          case "$name" in
            cgroup.kill-write|uidmap-helpers) ;;
            *) (( unexpected_unverified += 1 )) ;;
          esac
          ;;
      esac
      case "$name" in
        landlock-abi)
          (( generic_count += 1 ))
          generic_status=$status
          if [[ "$status" == PASS && "$detail" =~ ABI[[:space:]]+([0-9]+) ]]; then
            raw_abi=${BASH_REMATCH[1]}
            normalized_abi=$(tbound_normalize_decimal "$raw_abi") || normalized_abi=
          fi
          ;;
        d09-landlock-profile)
          (( d09_count += 1 ))
          d09_status=$status
          d09_detail=$detail
          [[ "$status" == FAIL ]] && (( d09_fail_count += 1 ))
          ;;
      esac
    fi
  done < "$report_file"

  if [[ "$row_count" -eq 0 ]]; then
    printf '2\n'
    return 0
  fi

  # Exit 3 applies only to the sole exact-profile FAIL, with a positive numeric
  # ABI, a generic PASS for that same ABI, and no unexpected UNVERIFIED rows.
  if [[ "$probe_rc" == 1 &&
        "$fail_count" -eq 1 &&
        "$generic_count" -eq 1 &&
        "$generic_status" == PASS &&
        "$d09_count" -eq 1 &&
        "$d09_fail_count" -eq 1 &&
        "$d09_detail" == *"requires exactly Landlock ABI 8"* &&
        -n "$normalized_abi" &&
        "$normalized_abi" != 0 &&
        "$normalized_abi" != 8 &&
        "$unexpected_unverified" -eq 0 ]]; then
    printf '3\n'
    return 0
  fi

  # Preserve FAIL precedence over UNVERIFIED, including mixed probe output.
  if [[ "$fail_count" -gt 0 ]]; then
    printf '1\n'
  elif [[ "$generic_count" -ne 1 || "$d09_count" -ne 1 ]] ||
       [[ "$generic_status" == PASS && ( "$normalized_abi" != 8 || "$d09_status" != PASS ) ]] ||
       [[ "$generic_status" == UNVERIFIED && "$d09_status" != UNVERIFIED ]]; then
    printf '2\n'
  elif [[ ! "$probe_rc" =~ ^(0|1|2)$ ]] ||
       [[ "$probe_rc" == 1 ]] ||
       [[ "$probe_rc" == 0 && "$unverified_count" -gt 0 ]] ||
       [[ "$probe_rc" == 2 && "$unverified_count" -eq 0 ]] ||
       [[ "$generic_status" == PASS && -z "$normalized_abi" ]]; then
    printf '2\n'
  elif [[ -n "$normalized_abi" && "$normalized_abi" != 8 ]]; then
    # A measured mismatch without the matching exact-profile FAIL row is
    # inconsistent output; fail closed rather than treating it as a pass.
    printf '1\n'
  elif [[ "$unexpected_unverified" -gt 0 ]]; then
    printf '4\n'
  else
    printf '0\n'
  fi
}
