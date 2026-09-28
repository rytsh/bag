export class UserRepo {
  findById(id: string) { return id; }
  static make(): UserRepo { return new UserRepo(); }
}
