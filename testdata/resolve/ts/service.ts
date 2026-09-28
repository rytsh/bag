import { UserRepo } from './repo';
export class UserService {
  constructor(private repo: UserRepo) {}
  get(id: string) {
    UserRepo.make();
    return this.repo.findById(id);
  }
}
