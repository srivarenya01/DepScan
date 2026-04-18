import requests

def main():
    r = requests.get('https://api.github.com/events')
    print("Status code:", r.status_code)
    try:
        events = r.json()
        if events:
            print("First event id:", events[0].get('id'))
    except Exception as e:
        print("Failed to decode json:", e)

if __name__ == '__main__':
    main()
