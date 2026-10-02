#!/usr/bin/env bash
# Local operator configuration, read as data (never sourced or evaluated).
# Only this allowlisted setting is supported; process presence wins over file.
load_viewer_memory_domains() {
    local file="$1" line key value file_value='' file_seen=false
    local process_present="${TRUVAG3_VIEWER_MEMORY_DOMAINS+x}"
    local process_value="${TRUVAG3_VIEWER_MEMORY_DOMAINS-}"
    if [[ -f "$file" ]]; then
        while IFS= read -r line || [[ -n "$line" ]]; do
            line="$(printf '%s' "$line" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')"
            [[ -z "$line" || "$line" == \#* ]] && continue
            [[ "$line" == *=* ]] || { printf 'Invalid viewer configuration line\n' >&2; return 1; }
            key="${line%%=*}"; value="${line#*=}"
            [[ "$key" == TRUVAG3_VIEWER_MEMORY_DOMAINS && "$file_seen" == false ]] || {
                printf 'Unsupported or duplicate viewer configuration key\n' >&2; return 1;
            }
            if [[ "$value" == \"*\" || "$value" == \'*\' ]]; then value="${value:1:${#value}-2}"; fi
            file_value="$value"; file_seen=true
        done < "$file"
    fi
    if [[ "$process_present" == x ]]; then
        value="$process_value"; VIEWER_MEMORY_DOMAINS_SOURCE=environment
    elif [[ "$file_seen" == true ]]; then
        value="$file_value"; VIEWER_MEMORY_DOMAINS_SOURCE=file
    else
        value=infrastructure; VIEWER_MEMORY_DOMAINS_SOURCE=default
    fi
    [[ "$value" != *$'\n'* && "$value" != *$'\r'* ]] || { printf 'Viewer domains must be a single comma-separated line\n' >&2; return 1; }
    local rest="$value" domain normalized=''
    while :; do
        domain="${rest%%,*}"
        domain="$(printf '%s' "$domain" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')"
        [[ -n "$domain" ]] || { printf 'Viewer domain entries must be non-empty\n' >&2; return 1; }
        normalized="${normalized:+$normalized,}$domain"
        [[ "$rest" == *,* ]] || break
        rest="${rest#*,}"
    done
    export TRUVAG3_VIEWER_MEMORY_DOMAINS="$normalized"
}
