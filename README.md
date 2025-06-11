# audio-bot

docker build -t audio-bot:latest .

docker run -d \
--name audio-bot \
-e TELEGRAM_BOT_TOKEN="7612642650:AAE-KVwvDLj6pouGhh6aVXJWOTGALXRF2w4" \
-e BOTHUB_API_TOKEN="eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpZCI6ImMyYjliOWQ0LTRlOGEtNDQ3MC1hYWMyLWNmZDRlNjE4NmZmZCIsImlzRGV2ZWxvcGVyIjp0cnVlLCJpYXQiOjE3MzEzNDYzNTcsImV4cCI6MjA0NjkyMjM1N30.IqKybFZtuKlS6pKcuPZQyMDJIWVtR1Bx2tRbqzJqPrA" \
-e YOUTUBE_COOKIES_PATH="/app/upload/cookies.txt" \
-v /home/user/vpomo/audio-bot/upload/cookies.txt:/app/upload/cookies.txt:rw \
audio-bot:latest

docker stop audio-bot
docker rm audio-bot

docker images
docker rmi abc123def456