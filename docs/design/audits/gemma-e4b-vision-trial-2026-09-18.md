# Gemma E4B vision trial — September 18 UTC

## Result

Do not promote `gemma-4-e4b` from this trial. All ten requests completed,
but every response differed from the inspected timing fields. The default
production prompt inferred invisible period labels in all five samples.
Correct final verdicts do not excuse fabricated evidence.

Control's live catalog supplies `gemma-4-e4b`, not an A4B identifier.
Production still pins the retired `gemma-4-12b`. The application release
does not change that configuration. Vision recovery needs a separately
approved, tested replacement; a code-health check cannot prove inference works.

## Method

The existing fixed-image benchmark used the real LLM adapter, three sampled
frames per request, strict response schema, model-owned sampling, reasoning
`none`, sequential calls and no retries. Expected times and gold annotations
were not sent to the model. Evaluation used FF-097's v4.

Five original-media frame sets were tested under two prompts: current default
and experimental period-role wording. Only the default is production behavior.
Gueye was sampled at 25/50/75% from the public TheGoalsZone clip. The other
four sets are the retained originals from the
[period-label benchmark](./period-label-recognition-2026-09-17.md).
This small, selected trial is not an estimated production error rate.

| Sample | Default prompt | Experimental period-role prompt |
|---|---|---|
| Gueye, 46:35/37/39 plus +4 | Verified, but invents 1H and omits +4 | Same transcription failure |
| Yamal, 44:48/51/53 without label, wrong for 89′ | Rejected; invents 1H and misreads later seconds | Rejected; still invents 1H and misreads one clock |
| Jesus, 77:27/29/31 without label | Rejects because it invents **1H** at minute 77 | Reads no label, but repeats first-frame time |
| Maitland-Niles, 95:04/15/27 plus +5′ | Verified; invents 2H and misreads first clock | Rejects: invents main 90 and secondary 05:00 |
| Fernández, 45:17/25/33 plus +1 | Verified; invents 1H and omits +1 | Same transcription failure |

Jesus is known wrong-fixture footage. The experimental verified outcome is
not a semantic success; the default rejection is also not evidence of
correct recognition, because its first-half observation is false.
Spelling differences such as +5 versus +5′ are less material than the
invented period and secondary timer.

Calls took 3.437–6.379 seconds. The completion marker records ten calls and
no transport failures. The experiment does not establish that E4B is always
worse than the removed model, or that another prompt/model will pass.
The useful next comparison is the catalog's `gemma-4-31b` on these same
inputs, plus explicit-period controls before any production model decision.

## Evidence and operation boundary

Local artifacts are under the ignored
`scratch-audit-2026-09-18/gueye-45plus2/` directory:

- `e4b-manifest.json`: SHA-256 `ec608431626aad5f8d66f2b34b5822bc0a61939a1cae4ee733cd437b5edac45d`
- `e4b-results.ndjson`: SHA-256 `650261bf4769fdeb0700103d7b11f48415e784d8818d683570eba11236f72970`

No production model configuration, prompt, policy, candidate, or media row
was changed by the trial. The [FF-097 recovery](./running-stoppage-period-2026-09-17.md)
remains separate: four Gueye posts, preserving old evidence and using normal
validation/deduplication rather than force-promotion. The existing fixture-wide
replay command is too broad for this proposed exact-candidate recovery.
