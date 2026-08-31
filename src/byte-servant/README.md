# Byte Servant

Minimal React chat app that calls the DeepSeek API directly from the browser. Runs in a single Docker container, locally.

## Run

```bash
docker build -t byte-servant ./src/byte-servant
docker run -p 5173:80 -e DEEPSEEK_API_KEY=sk-your-key-here byte-servant
```

Or with an env file:

```bash
# edit .env and put your real key in it
docker run -p 5173:80 --env-file .env byte-servant
```

Open [http://localhost:5173](http://localhost:5173).
