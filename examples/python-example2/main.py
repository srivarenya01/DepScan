from pydantic import BaseModel

class User(BaseModel):
    id: int
    name: str

    class Config:
        orm_mode = True


user_data = {"id": 1, "name": "John"}
user = User.from_orm(user_data)


print(user.dict())


print(user.json())
