module Billing
  class Base
    def compute; 1; end
    def process; compute; end
    def self.build; new; end
  end
end
