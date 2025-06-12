# audio-bot

docker build -t audio-bot:latest .

docker run -d \
--name audio-bot \
-e TELEGRAM_BOT_TOKEN="2w4" \
-e BOTHUB_API_TOKEN="qPrA" \
-e YOUTUBE_COOKIES_PATH="/app/upload/cookies.txt" \
-v /home/user/vpomo/audio-bot/upload/cookies.txt:/app/upload/cookies.txt:rw \
audio-bot:latest

docker stop audio-bot
docker rm audio-bot

docker images
docker rmi abc123def456