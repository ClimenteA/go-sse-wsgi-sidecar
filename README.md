# SSE WSGI Helper

Helper service which you can use along Django/Flask WSGI to handle Server Sent Events (SSE) with minimal resources to run.
Server Sent Events (SSE) work only in ASGI. Having this small service (built in Golang) helps implement SSE in WSGI python frameworks.

Until Django becomes full async this will help with implementing SSE in your web application. I've tried using Django async, but lost hot reload on browser and server - I couldn't make that compromise.


## How to implement SSE in Django

You will have a docker-compose.yml file with both the Django app and this service.

Add this in your .env file (both services need to have access to it use env_file in docker compose):

```
GO_SSE_SIDECAR_HOST=localhost
GO_SSE_SIDECAR_PORT=5687
GO_SSE_SIDECAR_REDIS_URL=redis://:your-password@localhost-or-docker-container-name:6379/0
GO_SSE_SIDECAR_TOKEN=secret-token-here
```

On the Python/Django app:
- Install `pyjwt` package - this will be used to make sure only the authentificated user can have access to the server sent events.
- Install `redis` package - we'll use here redis pub/sub functionality to have the Django app sending events and this app to send those recived events to frontend. If you already have celery/django-rq or other similar packages you could use the same connection as I did.


Add this view to your main `urls.py` file. This view will be used by frontend to get an authorization token.

```py

import jwt
import datetime
from django.conf import settings
from django.http import JsonResponse


@login_required
def get_sse_token_view(request):
    exp_dt = datetime.datetime.now(datetime.timezone.utc) + datetime.timedelta(
        minutes=15
    )

    payload = {
        "user_id": request.social_user_id,
        "exp": exp_dt,
    }

    try:
        token = jwt.encode(payload, settings.GO_SSE_SIDECAR_TOKEN, algorithm="HS256")
    except Exception as e:
        return JsonResponse({"error": str(e)}, status=500)

    return JsonResponse(
        {
            "token": token,
            "expires_in": 900,
        }
    )


urlpatterns = [
    # etc
    path("admin/", admin.site.urls),
    path("sse-token/", get_sse_token_view, name="sse_token"),
]

```

I've used the connection of django_rq because it was already in my setup, but you can create a new redis connection if you want.
It must be the same connection for both services so they can write to the same pub/sub server.

Add this somewhere in your utils package:

```py
import json

import django_rq


def publish(event_name: str, data: dict):
    r = django_rq.get_connection()
    r.publish("events", json.dumps({"event": event_name, "data": data}))

```

In your main `scripts.js` file or in `base.html` file add this `EventSource` listener.
You can change the urls based on what ports you've exposed. 
When you'll run the app entirely in docker compose change `localhost` with the name of the service (Django service and Go service). 
Add your handlers on `alert(JSON.stringify(e.data));` and make it do react on a new event however you want.


```js
let evtSource = null;

async function startSSE() {
  const res = await fetch("http://localhost:8000/sse-token");
  const { token } = await res.json();

  evtSource = new EventSource(`http://localhost:5687/sse-events?ssetoken=${token}`);

  evtSource.onmessage = (e) => {
    console.log("Received:", e.data);
    alert(JSON.stringify(e.data));
  };

  evtSource.onerror = (e) => {
    console.error("SSE error", e);
    evtSource.close();
    setTimeout(startSSE, 2000);
  };

}

startSSE();

```


Cool, now just import `publish` function where you need and start sending how many events you want to frontend.


## Why this is better than pooling? 

Having `setInterval` on frontend is a solution, but I've seen it so many times get stuck in a infinite loop (skill issue).
This method it's faster, consumes less resources and sends events only when that event really happends.

To avoid all this hassle you could just use FastAPI :))
