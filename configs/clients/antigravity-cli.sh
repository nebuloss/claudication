# Antigravity CLI (agy), pointed at a claudication gateway. Source it from
# your shell profile.
#
# agy's API-key mode: the key in GEMINI_API_KEY, a Gemini-compatible endpoint
# in GOOGLE_GEMINI_BASE_URL, and "modelProvider": "gemini" in
# ~/.gemini/antigravity-cli/settings.json, which the installer writes beside
# this file. Without that setting the key "has no effect", in Antigravity's
# own words. No /v1beta on the base URL: agy appends the path itself.

export GOOGLE_GEMINI_BASE_URL=https://claudication.example.com
export GEMINI_API_KEY=clc_...

# The models are agy's own: it offers its catalogue (agy models, /model,
# --model) and sends each choice as a base id — Gemini 3.8 Flash (High) as
# gemini-3.8-flash, its level as the thinking budget — and its side calls as
# gemini-3.1-flash-lite-preview. Those ids are this client's names on the
# gateway, and what each serves is chosen on the client's page, so there is
# nothing to name here.
