# bash completion for tbound and tbound-doctor.
# Install: source this file, or drop it in /etc/bash_completion.d/ or
# ~/.local/share/bash-completion/completions/.
_tbound() {
  local cur="${COMP_WORDS[COMP_CWORD]}"
  local sub="${COMP_WORDS[1]}"
  local cols
  case "$sub" in
    serve)
      cols="--pi --native-fixture --native-host-profile --help"
      ;;
    "")
      cols="serve --smoke-listen --socket-dir --help --version"
      ;;
    *)
      cols="--help"
      ;;
  esac
  COMPREPLY=( $(compgen -W "$cols" -- "$cur") )
}
complete -F _tbound tbound

_tbound_doctor() {
  local cur="${COMP_WORDS[COMP_CWORD]}"
  COMPREPLY=( $(compgen -W "--json --install-pi --prefix --node --pi --version --help" -- "$cur") )
}
complete -F _tbound_doctor tbound-doctor
