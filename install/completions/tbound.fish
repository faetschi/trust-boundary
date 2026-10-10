# fish completion for tbound and tbound-doctor.
# Install: copy to ~/.config/fish/completions/tbound.fish
complete -c tbound -f
complete -c tbound -n '__fish_use_subcommand' -a serve -d 'Run the supervisor (production refused until a signed profile exists)'
complete -c tbound -n '__fish_use_subcommand' -l smoke-listen -d 'Synthetic no-effect Unix-socket listener'
complete -c tbound -n '__fish_use_subcommand' -l socket-dir -r -d 'Private mode-0700 dir for the smoke socket'
complete -c tbound -n '__fish_seen_subcommand_from serve' -l pi -d 'Run the native Pi harness'
complete -c tbound -n '__fish_seen_subcommand_from serve' -l native-fixture -d 'Non-claim-bearing fixture'
complete -c tbound -n '__fish_seen_subcommand_from serve' -l native-host-profile -r -d 'Signed host profile receipt'

complete -c tbound-doctor -f
complete -c tbound-doctor -l json -d 'JSON output'
complete -c tbound-doctor -l install-pi -d 'Install pinned Pi packages into the prefix'
complete -c tbound-doctor -l prefix -r -d 'tbound prefix'
complete -c tbound-doctor -l node -r -d 'Explicit Node binary'
complete -c tbound-doctor -l pi -r -d 'Existing node_modules containing Pi'
