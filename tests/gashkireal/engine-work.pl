# Owned provider behavior for the integrated review engine. No vendor calls.
sub engine_work {
  my ($pointer_text) = @_;
  return unless -f "$d/engine-plan.json";
  if ($pointer_text eq '__owned_answer__') {
    open my $f, '<:raw', "$d/engine-plan.json" or die;
    my $plan=decode_json(do {local $/; <$f>}); close $f;
    my $mode=$plan->{answer_mode}//'answer';
    if ($mode ne 'clarification') {open my $h,'>>:raw','HUMAN.md' or die;print $h $plan->{answer_text};close $h;}
    if ($mode eq 'spec' || $mode eq 'questions') {open my $h,'>:raw',($mode eq 'spec'?'SPEC.md':'QUESTIONS.md') or die;print $h "Changed during answer turn.\n";close $h;}
    put('engine.answer-working',"$agent $mode\n");
    for (1..600) {last if -f "$d/engine.answer-continue";select(undef,undef,undef,0.05);}
    put('engine.answer-ended',"$agent $mode\n");
    return;
  }
  return unless $pointer_text =~ /^Read the file (".*") and do what it asks\.$/s;
  my $pointer = decode_json($1);
  my $load = sub { my ($path) = @_; open my $f, '<:raw', $path or die "$path: $!"; return decode_json(do { local $/; <$f> }); };
  my $write = sub { my ($path, $text) = @_; open my $f, '>:raw', $path or die "$path: $!"; print $f $text; close $f; };
  my $manifest = $load->('state/manifest.json');
  my $turn = $manifest->{current_turn};
  die 'engine turn absent' unless $turn->{id};
  my $request = $load->("state/turns/$turn->{id}/engine-request.json");
  die 'wrong provider' unless $request->{Provider} eq $agent;
  my $plan = $load->("$d/engine-plan.json");
  my $purpose = $turn->{purpose};
  $write->("$d/$turn->{id}.capture.json", encode_json({ provider=>$agent, argv=>\@ARGV, turn=>$turn, request=>$request, pointer=>$pointer }));
  put('engine.calls', "$agent $turn->{role} $purpose\n");
  if (($plan->{mutation_role}//'') eq $turn->{role} && (($plan->{mutation_purpose}//'') eq '' || $plan->{mutation_purpose} eq $purpose)) {
    $write->($plan->{mutation_path}, "Changed by owned pane stub.\n");
  }
  if ($turn->{role} eq 'planner') {
    $write->('SPEC.md', "# SPEC\nChecked $purpose result.\n") unless $purpose eq 'closing' && $plan->{closing_unchanged};
    if (($plan->{question_purpose}//'') eq $purpose) { $write->('QUESTIONS.md', "1. Choose A or B. Recommendation: A.\n"); }
    else { unlink 'QUESTIONS.md'; }
  } else {
    my $n = 0;
    if (open my $f, '<', "$d/engine.critics") { $n = <$f> // 0; close $f; }
    my $reply = "Review\nVERDICT: APPROVE\n";
    if ($purpose eq 'advisory') { $reply = $plan->{advisory} // $reply; }
    else { $reply = $plan->{critiques}[$n] // $reply; $write->("$d/engine.critics", $n+1); }
    $write->($request->{ExpectedArtifacts}[0], $reply);
  }
  if (-f "$d/engine.hold-$purpose") {
    put('engine.held', "$turn->{id}\n");
    for (1..600) { last if -f "$d/engine.continue"; select(undef,undef,undef,0.05); }
  }
}
