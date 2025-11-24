# BAJ AI

## Funny character AI using twitch reward buttons with:
- Control panel that allows you to skip unwanted requests.
- Custom characters with custom personalities and custom voices. (you can create your AI persona)
- Uses state of the art TTS and best role playing LLM models.
- Word filter List. You can filter words you don't want to listen or see, both from the chatter requested and from the AI responding.
- Different types of fun interactions like plain AI request, AI talking to each other, TTS, and forsen-style TTS with soundbites and filters.
- Obs script to add skip keybind.
- Forsen forsen forsen forsen forsen forsen forsen forsen forsen forsen.

## Plans:
- [x] Security features (added control panel sharing and fixed filters, so I guess it's done)
- [x] Add images to chars
- [x] Tune LLM
- [x] Obiwan voice
- [x] Switched to Index-TTS
- [x] AI talking to each other
- [ ] Integrate OpenMemory in all AI related flows
- [ ] Investigate ways to reduce VRAM footprint of index-tts-vllm
- [ ] TTS Streaming Support to reduce first word latency

## Bugs
- [x] Fix show image not working sometimes and not working when request is not active yet
- [x] Fix skip button stopping working
- [x] Make better images guide 
- [x] Show response when state of prompt image changes and show image visibility state
- [x] Move all static files(images and voice references) from postgres to minio
- [x] Full switch from production version to beta
- [ ] Refactor fundamental flaw in applying background filters. They only work on single audio/sfx right now. But they should be applied on full concatenated audio until filter is popped. Same goes for left to right/right to left filter.
- [ ] Fix AI agent unable to stop conversation in Agent BAJ reward.
