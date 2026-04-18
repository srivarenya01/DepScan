import requests

def process():
    r = requests.get('https://api.github.com/events')
    try:

        data = r.json()
        print(data)
    except ValueError:
        print("Standard JSON decoding failed")

if __name__ == "__main__":
    process()
