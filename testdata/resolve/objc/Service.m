#import "Repo.h"
@interface Service : NSObject
@property (nonatomic, strong) Repo *repo;
@end
@implementation Service
- (void)run {
  Repo *r = [Repo create];
  [r save:1];
  [self.repo save:2];
}
@end
